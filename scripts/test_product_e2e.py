"""Safety invariants for the product proof, not simulated provider inference."""

from argparse import Namespace
import unittest

from neopi_e2e import ProofError
import product_e2e


class ProductProofSafetyTest(unittest.TestCase):
    def test_empty_or_single_cycle_cannot_be_reported_as_repeated_proof(self):
        for cycles in (-1, 0, 1):
            with self.subTest(cycles=cycles), self.assertRaisesRegex(ProofError, 'at least two cycles'):
                product_e2e.exercise(Namespace(cycles=cycles, receipt='receipt.json'))

    def test_receipt_cannot_escape_the_private_scratch_directory(self):
        for filename in ('../receipt.json', '/home/shayna/.omp/agent/config.json', 'nested/receipt.json'):
            with self.subTest(filename=filename), self.assertRaisesRegex(ProofError, 'inside the private scratch'):
                product_e2e.exercise(Namespace(cycles=2, receipt=filename))


if __name__ == '__main__':
    unittest.main()
